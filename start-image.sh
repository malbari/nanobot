#!/bin/bash -x

export $(grep -v '^#' nanobot.env | xargs)

docker run --rm \
  -p 8080:8080 \
  -v $(pwd)/nanobot.yaml:/app/nanobot.yaml \
  -e OPENAI_API_KEY="$OPENAI_API_KEY" \
  -e OPENAI_BASE_URL="$OPENAI_BASE_URL" \
  -e NANOBOT_DEFAULT_MODEL="$NANOBOT_DEFAULT_MODEL" \
  -e AZURE_OPENAI_API_VERSION="$AZURE_OPENAI_API_VERSION" \
  -e OPENAI_CHAT_COMPLETION_API="$OPENAI_CHAT_COMPLETION_API" \
  nanobot:latest run /app/nanobot.yaml
