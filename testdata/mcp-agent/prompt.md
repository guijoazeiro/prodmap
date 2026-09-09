Use exclusively the MCP tools from the Prodmap server.

1. List deployments with environment=reference, service=payment-api,
   since=2026-09-01T19:03:00Z, and until=2026-09-01T19:05:00Z.
2. Extract the deployment_id of the most recent deployment returned. Do NOT
   produce the final response after list_deployments returns.
3. Your next required action is investigate_deployment. Call it using exactly:

   {
     "deployment": "<deployment_id returned by list_deployments>",
     "metric": "latency_p95",
     "before": "5m",
     "after": "5m",
     "min_samples": 4
   }

   `deployment` is the tool argument name. Do not use `deployment_id` as an
   argument name. Do not respond, summarize, or end the task between the two
   calls.

4. Only after investigate_deployment completes, copy from its response the
   three keys, before/after values, deltas, sample counts, status,
   classification, direction, confidence, and causality flag into the final
   JSON required by the JSON Schema.
5. Never infer or fabricate keys or values from the schema. If
   investigate_deployment is not called and completed, the task has failed;
   do not produce a final response.

Do not use shell, file reading, web, or any tool outside the Prodmap MCP
server. Do not claim causality.
