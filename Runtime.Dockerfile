FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY portal /app/portal
COPY web/dist /app/web
RUN chmod 0555 /app/portal && chmod -R a+rX /app/web && addgroup -g 10001 portal && adduser -D -u 10001 -G portal portal
USER 10001:10001
EXPOSE 18088
ENTRYPOINT ["/app/portal"]
