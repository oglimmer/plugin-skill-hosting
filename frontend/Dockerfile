FROM node:24.16.0-alpine AS build
WORKDIR /src
COPY package.json package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY . .
ARG VITE_APP_VERSION=""
ARG VITE_GIT_COMMIT=""
ARG VITE_BUILD_TIME=""
ENV VITE_APP_VERSION=${VITE_APP_VERSION}
ENV VITE_GIT_COMMIT=${VITE_GIT_COMMIT}
ENV VITE_BUILD_TIME=${VITE_BUILD_TIME}
RUN npm run build

FROM nginx:1.31-alpine
COPY --from=build /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY nginx-security-headers.conf /etc/nginx/snippets/security-headers.conf
EXPOSE 80
