FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY cli ./cli
COPY domain ./domain
COPY infra ./infra
COPY adapter ./adapter
COPY api ./api
COPY sdk ./sdk
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/filegate ./cmd/filegate

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/filegate /app/filegate
EXPOSE 8080/tcp
ENTRYPOINT ["/app/filegate"]
CMD ["serve"]
