FROM golang:1.26 AS builder
COPY . /app
WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential libsqlite3-dev \
    && rm -rf /var/lib/apt/lists/*
ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-Du_int8_t=uint8_t -Du_int16_t=uint16_t -Du_int64_t=uint64_t -Wno-incompatible-pointer-types"
RUN make && make clean-deps

FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ghostscript libsqlite3-0 ca-certificates \
    && rm -rf /var/lib/apt/lists/*
ENV GIN_MODE=release
ENV PORT=8000
COPY --from=builder /app/build/armi /usr/local/bin/armi
WORKDIR /app
ENTRYPOINT ["/usr/local/bin/armi"]
CMD ["serve"]
EXPOSE 8000
