import express from 'express';
import pg from 'pg';
import Redis from 'ioredis';

const pool = new pg.Pool({
  connectionString: process.env.DATABASE_URL ?? 'postgres://shop:shop@127.0.0.1:54329/shop',
});

async function seed() {
  await pool.query(`
    CREATE TABLE IF NOT EXISTS authors (id serial PRIMARY KEY, name text NOT NULL);
    CREATE TABLE IF NOT EXISTS books (id serial PRIMARY KEY, author_id int REFERENCES authors(id), title text NOT NULL, price numeric NOT NULL);
    CREATE TABLE IF NOT EXISTS orders (id serial PRIMARY KEY, book_id int REFERENCES books(id), email text NOT NULL, created_at timestamptz DEFAULT now());
  `);
  const { rows } = await pool.query('SELECT count(*)::int AS n FROM authors');
  if (rows[0].n > 0) return;
  for (let a = 1; a <= 12; a++) {
    const { rows: [author] } = await pool.query('INSERT INTO authors (name) VALUES ($1) RETURNING id', [`Author ${a}`]);
    for (let b = 1; b <= 3; b++) {
      await pool.query('INSERT INTO books (author_id, title, price) VALUES ($1, $2, $3)', [author.id, `Book ${a}.${b}`, 9 + a + b]);
    }
  }
}

const redis = new Redis(process.env.REDIS_URL ?? 'redis://127.0.0.1:63799');

const app = express();
app.use(express.json());

app.get('/api/authors', async (_req, res) => {
  const { rows: authors } = await pool.query('SELECT id, name FROM authors ORDER BY id');
  for (const author of authors) {
    const { rows } = await pool.query('SELECT id, title, price FROM books WHERE author_id = $1', [author.id]);
    author.books = rows;
  }
  res.json(authors);
});

app.get('/api/authors-fast', async (_req, res) => {
  const { rows } = await pool.query(`
    SELECT a.id, a.name, coalesce(json_agg(b ORDER BY b.id) FILTER (WHERE b.id IS NOT NULL), '[]') AS books
    FROM authors a LEFT JOIN books b ON b.author_id = a.id GROUP BY a.id ORDER BY a.id`);
  res.json(rows);
});

app.get('/api/books/:id', async (req, res) => {
  const key = `book:${req.params.id}`;
  const cached = await redis.get(key);
  if (cached) return res.json(JSON.parse(cached));
  const { rows } = await pool.query('SELECT * FROM books WHERE id = $1', [req.params.id]);
  if (!rows.length) return res.status(404).json({ error: 'not found' });
  await redis.set(key, JSON.stringify(rows[0]), 'EX', 5);
  res.json(rows[0]);
});

app.post('/api/orders', async (req, res) => {
  const { bookId, email } = req.body ?? {};
  const { rows } = await pool.query('INSERT INTO orders (book_id, email) VALUES ($1, $2) RETURNING id', [bookId, email]);
  res.status(201).json(rows[0]);
});

app.get('/api/packages/:name', async (req, res) => {
  const r = await fetch(`https://registry.npmjs.org/${encodeURIComponent(req.params.name)}/latest`);
  const pkg = await r.json();
  res.json({ name: pkg.name, version: pkg.version, description: pkg.description });
});

app.get('/api/report', async (_req, res) => {
  const out = {};
  for (const name of ['express', 'pg', 'fastify']) {
    const r = await fetch(`https://registry.npmjs.org/${name}/latest`);
    out[name] = (await r.json()).version;
  }
  const { rows } = await pool.query("SELECT pg_sleep(0.25), count(*)::int AS orders FROM orders WHERE created_at > now() - interval '1 day'");
  res.json({ versions: out, orders: rows[0].orders });
});

app.get('/api/checkout', async () => {
  await pool.query('SELECT id FROM orders ORDER BY id DESC LIMIT 1');
  throw new Error('payment provider rejected card: insufficient_funds');
});

app.use((err, _req, res, _next) => {
  console.error(err.message);
  res.status(500).json({ error: err.message });
});

await seed();
const port = Number(process.env.PORT ?? 3100);
app.listen(port, (err) => {
  if (err) throw err;
  console.log(`bookshop on http://localhost:${port}`);
});
