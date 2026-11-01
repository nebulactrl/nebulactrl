-- +goose Up
-- Per-team S3, registry, Loki and Grafana were retired in 0011, but their
-- containers kept running as ordinary services of each team's system project,
-- with their old credentials, and could be given a public domain again. Stop
-- them: desired_state 'stopped' makes the reconciler stop the containers and
-- keep the volumes, so the owner can still move the data by hand and then
-- delete the services. Headscale is the only platform service left: its services
-- are the ones platform_services still lists.
UPDATE services SET desired_state = 'stopped', updated_at = now()
WHERE deleted_at IS NULL
  AND desired_state <> 'stopped'
  AND environment_id IN (
      SELECT e.id FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.system
  )
  AND id NOT IN (SELECT unnest(service_ids) FROM platform_services);

-- +goose Down
-- Nothing to undo: a stopped service is started again from its service page.
SELECT 1;
