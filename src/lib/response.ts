export const json = <T>(data: T, status = 200) =>
  Response.json(data, { status });

export const error = (msg: string, status = 400) =>
  Response.json({ error: msg }, { status });

export const notFound = (msg = "not found") => error(msg, 404);
export const forbidden = (msg = "forbidden") => error(msg, 403);
export const badRequest = (msg: string) => error(msg, 400);
export const serverError = (msg = "internal error") => error(msg, 500);
