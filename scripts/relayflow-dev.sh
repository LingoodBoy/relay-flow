#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE_FILES=(
  -f docker-compose.infra.yml
  -f docker-compose.agent.yml
  -f docker-compose.relay.yml
)

INFRA_COMPOSE=(-f docker-compose.infra.yml)
AGENT_COMPOSE=(-f docker-compose.agent.yml)
RELAY_COMPOSE=(-f docker-compose.relay.yml)

usage() {
  cat <<'EOF'
Usage: scripts/relayflow-dev.sh <command>

Commands:
  up       Start infra, agent, Relay API, Worker and EventProcessor.
  down     Stop and remove containers, keep volumes.
  restart  Recreate all services, keep volumes.
  clean    Purge RabbitMQ queues and Redis current DB, keep containers/volumes.
  reset    Stop everything, remove compose volumes, then start from empty data.
  status   Show containers and RabbitMQ queue backlog.

Notes:
  clean keeps Grafana dashboards/settings and RabbitMQ/Redis volumes.
  reset deletes compose volumes, including RabbitMQ, Redis and Grafana data.
EOF
}

compose_all() {
  docker compose "${COMPOSE_FILES[@]}" "$@"
}

wait_container() {
  local name="$1"
  local seconds="${2:-60}"

  for _ in $(seq 1 "$seconds"); do
    if docker inspect "$name" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done

  echo "container not found: $name" >&2
  return 1
}

wait_healthy() {
  local name="$1"
  local seconds="${2:-120}"

  wait_container "$name" "$seconds"
  for _ in $(seq 1 "$seconds"); do
    local status
    status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$name" 2>/dev/null || true)"
    case "$status" in
      healthy|running)
        echo "$name is $status"
        return 0
        ;;
      unhealthy|exited|dead)
        echo "$name is $status" >&2
        docker logs "$name" --tail 80 >&2 || true
        return 1
        ;;
    esac
    sleep 1
  done

  echo "timeout waiting for $name to become healthy" >&2
  docker logs "$name" --tail 80 >&2 || true
  return 1
}

ensure_running() {
  local name="$1"
  local seconds="${2:-30}"

  wait_healthy "$name" "$seconds"
  sleep 2

  local status
  status="$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null || true)"
  if [[ "$status" != "running" ]]; then
    echo "$name is not running, current status: $status" >&2
    docker logs "$name" --tail 120 >&2 || true
    return 1
  fi
}

start_all() {
  echo "Starting infra..."
  docker compose "${INFRA_COMPOSE[@]}" up -d
  wait_healthy relayflow-rabbitmq-1 120
  wait_healthy relayflow-redis-1 120

  echo "Starting agent..."
  docker compose "${AGENT_COMPOSE[@]}" up -d --build
  wait_healthy relayflow-fastapi-agent-1 180

  echo "Starting relay services..."
  docker compose "${RELAY_COMPOSE[@]}" up -d --build
  ensure_running relayflow-gateway-1 60
  ensure_running relayflow-worker-1 60
  ensure_running relayflow-event-processor-1 60
}

purge_queue() {
  local queue="$1"

  if docker exec relayflow-rabbitmq-1 rabbitmqctl list_queues name --silent | grep -Fxq "$queue"; then
    docker exec relayflow-rabbitmq-1 rabbitmqctl purge_queue "$queue"
  else
    echo "queue not found, skip: $queue"
  fi
}

clean_data() {
  wait_container relayflow-rabbitmq-1 60
  wait_container relayflow-redis-1 60

  purge_queue relayflow.task.queue
  purge_queue relayflow.retry.queue
  purge_queue relayflow.dlq
  purge_queue relayflow.event.persist.queue

  docker exec relayflow-redis-1 redis-cli FLUSHDB
}

show_status() {
  compose_all ps
  echo
  if docker inspect relayflow-rabbitmq-1 >/dev/null 2>&1; then
    docker exec relayflow-rabbitmq-1 rabbitmqctl list_queues name messages_ready messages_unacknowledged messages
  fi
}

command="${1:-}"
case "$command" in
  up)
    start_all
    ;;
  down)
    compose_all down --remove-orphans
    ;;
  restart)
    compose_all down --remove-orphans
    start_all
    ;;
  clean)
    clean_data
    ;;
  reset)
    compose_all down -v --remove-orphans
    start_all
    ;;
  status)
    show_status
    ;;
  -h|--help|help|"")
    usage
    ;;
  *)
    echo "unknown command: $command" >&2
    usage
    exit 2
    ;;
esac
