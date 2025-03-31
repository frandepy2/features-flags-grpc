FROM golang:1.24.1-alpine

WORKDIR /app

COPY . .

RUN go mod tidy

EXPOSE 50051

CMD ["go", "run", "server/main.go"]