FROM oven/bun:1 AS base
WORKDIR /app

FROM base AS install
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile --production

FROM base AS release
COPY --from=install /app/node_modules ./node_modules
COPY src ./src
COPY package.json ./

# Must run as root to chown files to arbitrary UIDs/GIDs
EXPOSE 4000/tcp
CMD ["bun", "run", "src/index.ts"]
