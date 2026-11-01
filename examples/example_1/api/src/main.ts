import 'reflect-metadata';
import { NestFactory } from '@nestjs/core';
import { AppModule } from './module';

async function main() {
  const app = await NestFactory.create(AppModule);
  app.enableShutdownHooks();
  await app.listen(Number(process.env.PORT || 3001), '0.0.0.0');
}

void main();
