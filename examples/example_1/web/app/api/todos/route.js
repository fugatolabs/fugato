import { proxy } from './proxy';

export const dynamic = 'force-dynamic';
export function GET(request) { return proxy('/todos', request); }
export function POST(request) { return proxy('/todos', request); }
