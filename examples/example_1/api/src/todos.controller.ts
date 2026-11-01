import { BadRequestException, Body, Controller, Delete, Get, HttpCode, Param, ParseIntPipe, Patch, Post } from '@nestjs/common';
import { TodosService } from './todos.service';

@Controller()
export class TodosController {
  constructor(private readonly todos: TodosService) {}

  @Get('hello')
  hello() {
    return { message: process.env.EXAMPLE_GREETING || 'EXAMPLE_GREETING is missing from .env' };
  }

  @Get('health')
  health() {
    return this.todos.health();
  }

  @Get('todos')
  list() {
    return this.todos.list();
  }

  @Post('todos')
  create(@Body() body: { title?: unknown }) {
    const title = typeof body?.title === 'string' ? body.title.trim() : '';
    if (!title || title.length > 200) throw new BadRequestException('title must contain 1–200 characters');
    return this.todos.create(title);
  }

  @Patch('todos/:id')
  complete(@Param('id', ParseIntPipe) id: number, @Body() body: { completed?: unknown }) {
    if (typeof body?.completed !== 'boolean') throw new BadRequestException('completed must be a boolean');
    return this.todos.complete(id, body.completed);
  }

  @Delete('todos/:id')
  @HttpCode(204)
  remove(@Param('id', ParseIntPipe) id: number) {
    return this.todos.remove(id);
  }
}
