export const dynamic = 'force-dynamic';

export async function GET() {
  try {
    const response = await fetch(`${process.env.API_URL}/health`, { cache: 'no-store' });
    const api = await response.json();
    return Response.json({ ok: response.ok && api.ok, api }, { status: response.ok && api.ok ? 200 : 503 });
  } catch {
    return Response.json({ ok: false }, { status: 503 });
  }
}
