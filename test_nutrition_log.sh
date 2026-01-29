#!/bin/bash
# Script to simulate sending a nutrition log via Telegram bot

# This would normally be done through Telegram's API
# For testing, let's just check the logs when a message is received

echo "To test nutrition followup:"
echo "1. Send a message to your nutrition log bot on Telegram"
echo "2. Check the bot logs:"
echo "   docker-compose logs --tail=100 bot | grep -E '(nutrition|review|follow)'"
echo ""
echo "Expected logs when it works:"
echo "  - 'nutrition review triggered for agent=nutrition logChat=... targets=[...]'"
echo "  - 'nutrition follow-up starting for chat ...'"
echo "  - 'nutrition follow-up completed successfully for chat ...'"
echo ""
echo "If you see 'nutrition review not triggered', check:"
echo "  - Is the message going to the log chat (8536615205)?"
echo "  - Is the agent name 'nutrition'?"
echo "  - Is the followup function configured?"
