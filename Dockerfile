# ---- build frontend ----
FROM node:20-alpine AS frontend
WORKDIR /fe
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
RUN npm run build

# ---- build backend ----
FROM golang:1.23-alpine AS backend
WORKDIR /src
COPY backend/ ./
RUN CGO_ENABLED=0 go build -o /datahunter ./cmd/server

# ---- final image ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=backend /datahunter /datahunter
COPY --from=frontend /fe/dist /web
ENV STATIC_DIR=/web
ENV PORT=8080
EXPOSE 8080
CMD ["/datahunter"]
