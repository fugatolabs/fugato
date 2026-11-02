"""Synthetic projects and example locations used by acceptance scenarios."""
from .harness import SOURCE

EXAMPLE = SOURCE / "examples/example_2"

WEB_ENVIRONMENT = """version: 1
start:
  services: [web]
endpoints:
  website:
    service: web
    port: 3000
    protocol: http
    browser: true
"""


SERVER = """const http=require('node:http');
http.createServer((req,res)=>{res.writeHead(200,{'content-type':'application/json'});
res.end(JSON.stringify({ok:true,path:req.url}));}).listen(3000,'0.0.0.0');
"""
WEB_ONLY = """services:
  web:
    image: node:20-alpine
    working_dir: /app
    command: ["node", "server.js"]
    expose: ["3000"]
    volumes: ["./server.js:/app/server.js:ro"]
"""
