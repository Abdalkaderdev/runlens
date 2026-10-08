import sqlite3
import time
from pathlib import Path

import httpx
from fastapi import FastAPI, HTTPException

DB = Path(__file__).with_name("catalog.db")

app = FastAPI(title="catalog")


def db():
    conn = sqlite3.connect(DB)
    conn.row_factory = sqlite3.Row
    return conn


def seed():
    with db() as conn:
        conn.executescript(
            """
            CREATE TABLE IF NOT EXISTS products (id INTEGER PRIMARY KEY, name TEXT NOT NULL, price REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS reviews (id INTEGER PRIMARY KEY, product_id INTEGER NOT NULL, stars INTEGER NOT NULL, body TEXT);
            """
        )
        if conn.execute("SELECT count(*) FROM products").fetchone()[0]:
            return
        for p in range(1, 16):
            conn.execute("INSERT INTO products (name, price) VALUES (?, ?)", (f"Product {p}", 5.0 + p))
            for r in range(3):
                conn.execute(
                    "INSERT INTO reviews (product_id, stars, body) VALUES (?, ?, ?)",
                    (p, 1 + (p + r) % 5, f"review {r} of product {p}"),
                )


seed()


@app.get("/products")
def products():
    conn = db()
    rows = [dict(r) for r in conn.execute("SELECT id, name, price FROM products ORDER BY id")]
    for row in rows:
        row["reviews"] = [dict(r) for r in conn.execute("SELECT stars, body FROM reviews WHERE product_id = ?", (row["id"],))]
    return rows


@app.get("/products/{product_id}")
def product(product_id: int):
    row = db().execute("SELECT id, name, price FROM products WHERE id = ?", (product_id,)).fetchone()
    if row is None:
        raise HTTPException(404, "product not found")
    return dict(row)


@app.get("/pypi/{name}")
def pypi(name: str):
    r = httpx.get(f"https://pypi.org/pypi/{name}/json", timeout=10)
    info = r.json()["info"]
    return {"name": info["name"], "version": info["version"], "summary": info["summary"]}


@app.get("/slow")
def slow():
    db().execute("SELECT count(*) FROM reviews r JOIN products p ON p.id = r.product_id").fetchone()
    time.sleep(0.6)
    return {"ok": True}


@app.get("/boom")
def boom():
    db().execute("SELECT id FROM products WHERE price > ?", (100,)).fetchall()
    raise RuntimeError("inventory service returned malformed payload")
