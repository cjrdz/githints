# syntax=docker/dockerfile:1
ARG NODE_VERSION=22
FROM node:${NODE_VERSION}-alpine AS deps
WORKDIR /app
COPY package.json ./
RUN npm ci

FROM deps AS build
RUN npm run build

FROM gcr.io/distroless/nodejs22-debian12
COPY --from=build /app/dist /app
# FROM ubuntu:commented-out
CMD ["/app/server.js"]
