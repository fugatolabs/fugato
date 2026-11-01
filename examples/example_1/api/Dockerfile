FROM node:24-bookworm-slim
# Nest's watcher uses ps to stop the whole child process tree before restarting.
RUN apt-get update && apt-get install -y --no-install-recommends procps \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY nest-cli.json tsconfig.json ./
COPY src ./src
EXPOSE 3001
CMD ["npm", "run", "dev"]
