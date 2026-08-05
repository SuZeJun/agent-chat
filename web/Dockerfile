# syntax=docker/dockerfile:1

FROM node:20.19.5-alpine AS dependencies
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:20.19.5-alpine AS builder
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=dependencies /app/node_modules ./node_modules
COPY . .
RUN npm run build

FROM node:20.19.5-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    HOSTNAME=0.0.0.0 \
    PORT=3000

RUN addgroup --system --gid 10001 agentchat \
    && adduser --system --uid 10001 --ingroup agentchat agentchat

COPY --from=builder --chown=agentchat:agentchat /app/.next/standalone ./
COPY --from=builder --chown=agentchat:agentchat /app/.next/static ./.next/static

USER agentchat
EXPOSE 3000
CMD ["node", "server.js"]
