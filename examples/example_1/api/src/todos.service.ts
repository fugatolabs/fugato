import { BadRequestException, Injectable, NotFoundException, OnModuleDestroy, OnModuleInit } from '@nestjs/common';
import { Pool } from 'pg';

@Injectable()
export class TodosService implements OnModuleInit, OnModuleDestroy {
  private readonly pool = new Pool({ connectionString: process.env.DATABASE_URL });

  async onModuleInit() {
    await this.pool.query('CREATE TABLE IF NOT EXISTS todos (id SERIAL PRIMARY KEY, title VARCHAR(200) NOT NULL, completed BOOLEAN NOT NULL DEFAULT FALSE)');
  }

  async onModuleDestroy() {
    await this.pool.end();
  }

  async health() {
    await this.pool.query('SELECT 1 FROM todos LIMIT 1');
    return { ok: true };
  }

  async list() {
    const result = await this.pool.query('SELECT id, title, completed FROM todos ORDER BY id');
    return result.rows;
  }

  async create(title: string) {
    const result = await this.pool.query(
      'INSERT INTO todos (title) VALUES ($1) RETURNING id, title, completed', [title],
    );
    return result.rows[0];
  }

  async complete(id: number, completed: boolean) {
    this.validateId(id);
    const result = await this.pool.query(
      'UPDATE todos SET completed = $2 WHERE id = $1 RETURNING id, title, completed', [id, completed],
    );
    if (!result.rowCount) throw new NotFoundException('Todo not found');
    return result.rows[0];
  }

  async remove(id: number) {
    this.validateId(id);
    const result = await this.pool.query('DELETE FROM todos WHERE id = $1', [id]);
    if (!result.rowCount) throw new NotFoundException('Todo not found');
  }

  private validateId(id: number) {
    if (!Number.isSafeInteger(id) || id < 1 || id > 2147483647) {
      throw new BadRequestException('id must be a positive 32-bit integer');
    }
  }
}
