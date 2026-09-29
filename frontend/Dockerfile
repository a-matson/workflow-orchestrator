# Build stage: compile Vue 3 frontend with TypeScript
FROM node:24-alpine AS build

WORKDIR /app

# Copy dependency manifest and lock file
COPY package.json package-lock.json ./

# Install dependencies deterministically
RUN npm ci

# Copy source code
COPY . .

# Build the Vue application
# This runs vue-tsc type checks and Vite bundling
RUN npm run build

# Runtime stage: serve with nginx
FROM nginx:1.27-alpine

# Copy built application from build stage
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
