export async function proxy(path, request) {
  try {
    const response = await fetch(`${process.env.API_URL}${path}`, {
      method: request.method,
      cache: 'no-store',
      ...(request.method === 'POST' || request.method === 'PATCH' ? {
        headers: { 'Content-Type': 'application/json' },
        body: await request.text(),
      } : {}),
    });
    return new Response(response.status === 204 ? null : response.body, {
      status: response.status,
      ...(response.status === 204 ? {} : { headers: { 'Content-Type': 'application/json' } }),
    });
  } catch {
    return Response.json({ message: 'The API is unavailable. Please try again.' }, { status: 502 });
  }
}
