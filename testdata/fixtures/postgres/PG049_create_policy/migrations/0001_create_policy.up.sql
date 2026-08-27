CREATE POLICY tenant_isolation ON orders USING (tenant_id = current_setting('app.tenant')::bigint);
