# Boardly's UI as a production image: built once here, served by nginx.
# The API's address is NOT baked in — docker/40-runtime-config.sh writes it into
# config.js from the container's environment at every start, so one image serves
# any environment and changing the address needs a restart, not a rebuild.

FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY docker/40-runtime-config.sh /docker-entrypoint.d/40-runtime-config.sh
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
