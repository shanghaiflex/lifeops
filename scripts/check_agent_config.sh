#!/bin/bash
# Health check script to verify agent configurations

set -e

echo "=== Agent Configuration Health Check ==="
echo ""

# Check environment variables for each agent
agents=("COACH" "SLEEP" "NUTRITION" "FINANCE")

for agent in "${agents[@]}"; do
    echo "Agent: $agent"

    # Check telegram token
    token_var="${agent}_TELEGRAM_TOKEN"
    if [ -n "${!token_var}" ]; then
        echo "  ✓ Telegram token: configured"
    else
        echo "  ✗ Telegram token: MISSING"
    fi

    # Check chat ID
    chat_id_var="${agent}_TELEGRAM_CHAT_ID"
    if [ -n "${!chat_id_var}" ]; then
        echo "  ✓ Chat ID: ${!chat_id_var}"
    else
        echo "  ✗ Chat ID: MISSING"
    fi

    # Check YAML file
    yaml_file="data/telegram_agents/$(echo $agent | tr '[:upper:]' '[:lower:]').yaml"
    if [ -f "$yaml_file" ]; then
        echo "  ✓ Config file: $yaml_file exists"

        # Check if chat_id is configured in YAML
        if grep -q "^chat_id:" "$yaml_file"; then
            chat_id_value=$(grep "^chat_id:" "$yaml_file" | cut -d: -f2- | tr -d ' ')
            echo "  ✓ YAML chat_id: $chat_id_value"
        else
            echo "  ✗ YAML chat_id: not configured"
        fi
    else
        echo "  ✗ Config file: $yaml_file NOT FOUND"
    fi

    echo ""
done

# Special checks for nutrition agent
echo "=== Nutrition Agent Special Configuration ==="
echo "  Log Chat ID: ${NUTRITION_LOG_CHAT_ID:-NOT SET}"
echo "  Review Chat ID: ${NUTRITION_REVIEW_CHAT_ID:-NOT SET}"
echo "  Log Token: ${NUTRITION_LOG_TELEGRAM_TOKEN:+configured}"
echo "  Review Token: ${NUTRITION_REVIEW_TELEGRAM_TOKEN:+configured}"
echo ""

echo "=== Database Connection ==="
if [ -n "$POSTGRES_DSN" ]; then
    echo "  ✓ POSTGRES_DSN: configured"
else
    echo "  ✗ POSTGRES_DSN: MISSING"
fi
echo ""

echo "Done!"
