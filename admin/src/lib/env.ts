export type AdminEnv = {
  filegateUrl: string;
  filegateToken: string;
  adminToken: string;
  sessionSecret: string;
  port: number;
};

function required(name: string): string {
  const value = Bun.env[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

export function env(): AdminEnv {
  const filegateToken = required("FILEGATE_TOKEN");
  return {
    filegateUrl: required("FILEGATE_URL"),
    filegateToken,
    adminToken: Bun.env.ADMIN_TOKEN?.trim() || filegateToken,
    sessionSecret: Bun.env.ADMIN_SESSION_SECRET?.trim() || filegateToken,
    port: Number(Bun.env.PORT || 3000),
  };
}
