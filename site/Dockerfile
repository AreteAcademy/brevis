FROM nginx:alpine

COPY . /usr/share/nginx/html

# `try_files ... /index.html` devolveria a landing com status 200 para qualquer
# caminho errado, e um link quebrado passaria despercebido. Aqui um caminho
# inexistente é 404 de verdade, servido pela página 404 gerada.
# nginx's mime.types has no .md, and the agent-facing Markdown was served as
# octet-stream -- which a client that honours the type DOWNLOADS instead of
# reading. This appends to the table; a `types` block inside `server` would
# REPLACE it, which is how the first attempt turned every .html into a download.
RUN sed -i 's|^    text/plain  *txt;|    text/plain                            txt;\n    text/markdown                         md;|' /etc/nginx/mime.types \
 && grep -q 'text/markdown' /etc/nginx/mime.types

RUN cd /usr/share/nginx/html \
 && rm -rf build.py templates content i18n.json Dockerfile README.md \
           netlify.toml vercel.json .lighthouserc.json .gitignore \
 && printf '%s\n' \
  'server {' \
  '  listen 80;' \
  '  root /usr/share/nginx/html;' \
  '  index index.html;' \
  '  error_page 404 /404.html;' \
  '  charset utf-8;' \
  '  charset_types text/markdown text/plain text/css application/javascript;' \
  '  location / { try_files $uri $uri/ =404; }' \
  '  location /assets/ { expires 1y; add_header Cache-Control "public, immutable"; }' \
  '  location /css/    { expires 1y; add_header Cache-Control "public, immutable"; }' \
  '  location /js/     { expires 1y; add_header Cache-Control "public, immutable"; }' \
  '  gzip on;' \
  '  gzip_types text/html text/css application/javascript application/json image/svg+xml;' \
  '}' > /etc/nginx/conf.d/default.conf

EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
