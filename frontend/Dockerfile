# dist/ is platform-independent, so build it natively instead of under QEMU per arch.
FROM --platform=$BUILDPLATFORM node:24-alpine AS build

WORKDIR /app

# Copy only package files first so dependency layer caches across source edits
COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build

FROM nginx:1.29-alpine

# nginx.conf is bind-mounted by docker-compose.yml from repo root (outside this build context)
COPY --from=build /app/dist /usr/share/nginx/html
