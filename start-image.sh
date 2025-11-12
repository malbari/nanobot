#!/bin/bash

PORT=30008
NAME="nanobot"

export $(grep -v '^#' nanobot.env | xargs)

docker run -d --restart unless-stopped -it \
  --add-host=host.docker.internal:host-gateway \
  --name $NAME \
  -p $PORT:$PORT \
  -v $(pwd)/nanobot.yaml:/app/nanobot.yaml \
  -e NANOBOT_RUN_LISTEN_ADDRESS="0.0.0.0:$PORT" \
  -e OPENAI_API_KEY="$OPENAI_API_KEY" \
  -e OPENAI_BASE_URL="$OPENAI_BASE_URL" \
  -e NANOBOT_DEFAULT_MODEL="$NANOBOT_DEFAULT_MODEL" \
  -e AZURE_OPENAI_API_VERSION="$AZURE_OPENAI_API_VERSION" \
  -e OPENAI_CHAT_COMPLETION_API="$OPENAI_CHAT_COMPLETION_API" \
  nanobot:latest run /app/nanobot.yaml
