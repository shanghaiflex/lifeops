# Agent Messaging Fix - Summary

## Issues Identified

### 1. Coach Agent Not Sending Messages
**Problem**: Coach agent sent 0 messages despite being configured with scheduled review times.

**Root Cause**: The `RunOnce()` method queries the database for chat history to determine which chats to message. Agents without any prior chat history get an empty result, and while there's fallback logic to use `defaultChatIDs`, this was failing silently.

### 2. Sleep Agent Sending Too Many Messages
**Problem**: Sleep agent sent 7 messages in one day.

**Explanation**: This is actually expected behavior:
- 2 scheduled daily reviews (09:00, 22:00)
- ~5 event-triggered messages when new sleep data was ingested via API

### 3. Nutritionist Not Sending Follow-up After Food Logging
**Problem**: When logging food to the nutrition log agent, expected a follow-up message from the nutritionist but didn't receive one.

**Root Cause**: The followup mechanism depends on proper configuration of the review bot token and chat IDs. The code has the logic in place, but execution might be failing silently.

## Changes Made

### 1. Enhanced Logging (commit 6fc30b3)

Added comprehensive diagnostic logging to:
- Worker initialization (shows `defaultChatIDs`)
- `RunOnce()` method (shows DB vs resolved chat IDs)
- Nutrition followup triggering (shows whether followup is configured and being called)

**Key Log Patterns to Look For:**

```
worker[coach] initialized with defaultChatIDs=[182466300]
worker[coach] no chat IDs resolved (db returned 0, defaults=[182466300])
```

If you see `defaults=[]`, the configuration is not loading correctly.

```
worker[coach] running for 1 chat(s): [182466300]
worker[coach] starting daily review for chat 182466300
worker[coach] successfully completed daily review for chat 182466300
```

This shows successful execution.

```
nutrition review not triggered: agent=nutrition shouldTrigger=true hasFollowUp=false
```

This means the followup function was not configured properly.

```
nutrition review triggered for agent=nutrition logChat=8536615205 targets=[182466300]
nutrition follow-up starting for chat 182466300
nutrition follow-up completed successfully for chat 182466300
```

This shows the nutrition followup working correctly.

### 2. Improved Error Handling (commit 24df450)

- Enhanced `resolveChatIDs()` with defensive fallback logic
- Added context to all error messages (which step failed)
- Improved logging throughout the daily review execution

**Key Improvements:**
- Cold start handling: Uses `defaultChatIDs` when no chat history exists
- Defensive fallback: If filtering removes all chat IDs, falls back to original list
- Better error messages: Each step that can fail now includes context

### 3. Configuration Health Check Script

Added `scripts/check_agent_config.sh` to verify agent configurations.

Run it to check:
- Telegram tokens are set
- Chat IDs are configured
- YAML files exist and are properly formatted
- Special nutrition agent configuration

## How to Use the Logs

### Check if an agent is properly initialized:

```bash
docker logs lifeops-app 2>&1 | grep "worker\[coach\] initialized"
```

Look for the `defaultChatIDs` value - it should contain your chat ID.

### Check if scheduled reviews are running:

```bash
docker logs lifeops-app 2>&1 | grep "worker\[coach\] running for"
```

This shows when the agent is actually triggered.

### Check for execution errors:

```bash
docker logs lifeops-app 2>&1 | grep -E "(worker\[coach\].*error|worker\[coach\].*failed)"
```

### Check nutrition followup:

```bash
docker logs lifeops-app 2>&1 | grep "nutrition.*follow-up"
```

## Expected Behavior After Fix

### Coach Agent (and other scheduled agents)
- Should send messages at configured times (09:00, 21:00 for coach)
- Logs will show initialization with chat ID
- Logs will show when reviews are triggered and completed
- If cold start (no history), will use `defaultChatIDs` from config

### Sleep Agent
- Scheduled messages: 2x daily (09:00, 22:00)
- Event-triggered: After new sleep data is ingested
- This is normal and expected - you can adjust triggers if needed

### Nutrition Agent
- When you log food to log chat (8536615205):
  - Saves the food entry
  - Triggers followup to review chat (182466300)
  - Followup worker runs with latest nutrition data
  - Sends analysis message to review chat

## Verification Steps

1. **Run the health check:**
   ```bash
   ./scripts/check_agent_config.sh
   ```

2. **Check logs after deployment:**
   ```bash
   # Check if workers are initialized properly
   docker logs lifeops-app 2>&1 | grep "initialized with defaultChatIDs"

   # Check if scheduled reviews are running
   docker logs lifeops-app 2>&1 | grep "running for.*chat"

   # Check for any errors
   docker logs lifeops-app 2>&1 | grep -i error | grep worker
   ```

3. **Test nutrition followup:**
   - Send a food log to the nutrition log bot
   - Check logs for "nutrition follow-up starting"
   - Check review chat for the analysis message

4. **Test coach agent:**
   - Wait for scheduled review time (09:00 or 21:00 Moscow time)
   - Check logs for "worker[coach] running"
   - Check your main chat for the coach message

## Troubleshooting

### If coach agent still doesn't send messages:

1. Check logs for:
   ```
   worker[coach] no chat IDs resolved (db returned 0, defaults=[])
   ```
   This means `COACH_TELEGRAM_CHAT_ID` is not set or not loading.

2. Verify environment variable is set:
   ```bash
   echo $COACH_TELEGRAM_CHAT_ID
   ```

3. Check the YAML file has the correct reference:
   ```yaml
   chat_id: ${COACH_TELEGRAM_CHAT_ID}
   ```

4. Restart the application to reload configuration.

### If nutrition followup doesn't work:

1. Check logs for:
   ```
   nutrition review not triggered: agent=nutrition shouldTrigger=true hasFollowUp=false
   ```
   This means the followup function wasn't configured during bot initialization.

2. Verify that:
   - `NUTRITION_REVIEW_TELEGRAM_TOKEN` is set
   - `NUTRITION_REVIEW_CHAT_ID` is set (should be 182466300)
   - The nutrition bot is running

3. Check for errors during nutrition bot initialization:
   ```bash
   docker logs lifeops-app 2>&1 | grep "nutrition.*review sender"
   ```

## Configuration Reference

Current configuration (from environment variables):

```bash
COACH_TELEGRAM_CHAT_ID=182466300
NUTRITION_TELEGRAM_CHAT_ID=182466300
NUTRITION_LOG_CHAT_ID=8536615205
NUTRITION_REVIEW_CHAT_ID=182466300
```

Agent schedules:
- Coach: 09:00, 21:00 Moscow time
- Sleep: 09:00, 22:00 Moscow time
- Nutrition: 09:00 Moscow time (scheduled review)
- Nutrition followup: After each food log (event-triggered)

## Next Steps

1. Deploy these changes to your environment
2. Monitor logs for the next 24 hours
3. Verify agents are sending messages at scheduled times
4. Test nutrition followup by logging a meal

If issues persist after deployment, the enhanced logging will show exactly where the problem is occurring.
