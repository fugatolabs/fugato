FROM node:26-bookworm-slim
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY app ./app
EXPOSE 3000
CMD ["npm", "run", "dev"]
