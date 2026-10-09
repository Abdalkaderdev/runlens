const base = process.env.BASE ?? 'http://localhost:3100';
const paths = [
  ['GET', '/api/authors'],
  ['GET', '/api/authors-fast'],
  ['GET', '/api/books/3'],
  ['GET', '/api/books/7'],
  ['GET', '/api/books/999'],
  ['GET', '/api/packages/express'],
  ['GET', '/api/report'],
  ['GET', '/api/checkout'],
  ['POST', '/api/orders'],
];
const rounds = Number(process.argv[2] ?? 20);
for (let i = 0; i < rounds; i++) {
  const [method, path] = paths[Math.floor(Math.random() * paths.length)];
  const body = method === 'POST' ? JSON.stringify({ bookId: 1 + (i % 30), email: `reader${i}@example.com` }) : undefined;
  const r = await fetch(base + path, { method, body, headers: { 'content-type': 'application/json' } }).catch((e) => ({ status: e.message }));
  console.log(method, path, r.status);
  await new Promise((ok) => setTimeout(ok, 150 + Math.random() * 400));
}
