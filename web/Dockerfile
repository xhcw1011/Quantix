# ─── Build stage ──────────────────────────────────────────────────────────────
# Build context must be the project root (set in docker-compose: context: ..)
FROM node:20-alpine AS builder

WORKDIR /app

COPY web/package*.json ./
RUN npm ci

COPY web/ .
RUN npm run build

# ─── Runtime stage (nginx) ────────────────────────────────────────────────────
FROM nginx:1.27-alpine

COPY --from=builder /app/dist /usr/share/nginx/html
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
