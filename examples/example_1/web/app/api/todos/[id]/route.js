import { proxy } from '../proxy';

export const dynamic = 'force-dynamic';

async function forward(request, { params }) {
  const { id } = await params;
  return proxy(`/todos/${encodeURIComponent(id)}`, request);
}

export const PATCH = forward;
export const DELETE = forward;
