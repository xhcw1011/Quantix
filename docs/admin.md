# Admin Operations Manual

## Overview

The Quantix admin system allows privileged users to manage all platform users and force-stop any trading engine regardless of owner.

## Initial Setup

### Apply DB Migration

```bash
psql -U quantix -d quantix -f migrations/004_admin.sql
```

This adds `role TEXT DEFAULT 'user'` and `is_active BOOLEAN DEFAULT TRUE` columns to the `users` table.

### Create Admin User

```bash
go run ./cmd/api \
  -config config/config.yaml \
  -create-admin \
  -admin-username admin \
  -admin-password "StrongPass123!" \
  -admin-email "admin@yourcompany.com"
```

Output: `Admin user created: id=1 username=admin`

## Admin API Endpoints

All admin endpoints require a valid JWT (`Authorization: Bearer <token>`) from an account with `role = 'admin'`.

### List All Users

```
GET /api/admin/users
```

Response:
```json
[
  {
    "id": 1,
    "username": "admin",
    "email": "admin@example.com",
    "role": "admin",
    "is_active": true,
    "created_at": "2025-01-01T00:00:00Z",
    "running_engines": 0
  },
  {
    "id": 2,
    "username": "trader1",
    "email": "trader1@example.com",
    "role": "user",
    "is_active": true,
    "created_at": "2025-06-15T10:30:00Z",
    "running_engines": 2
  }
]
```

### Activate / Deactivate User

```
PUT /api/admin/users/{id}/activate
Body: {"active": false}
```

- Deactivating a user also force-stops all their running engines.
- Admin accounts cannot be deactivated via the UI (the button is disabled).
- Deactivated users receive `403 Forbidden` on login.

### List All Running Engines

```
GET /api/admin/engines
```

Returns all engines across all users, including `user_id` field.

### Force Stop Engine

```
DELETE /api/admin/engines/{user_id}/{engine_id}
```

Immediately cancels the engine context. The engine goroutine will clean up asynchronously.

Example:
```bash
curl -X DELETE http://localhost:8080/api/admin/engines/2/BTCUSDT-1h-macross \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

## Admin UI

The Admin panel is accessible at `/admin` in the web frontend (only visible to admin-role accounts).

### Users Tab
- Lists all registered users with role badge, active status, and running engine count
- Activate/Deactivate toggle (disabled for admin accounts)

### Running Engines Tab
- Shows all engines across all users with user ID, mode, leverage, and start time
- Force Stop button per engine

## Role Management

Roles are stored in the `users.role` column (`"user"` or `"admin"`). To promote an existing user to admin:

```bash
psql -U quantix -d quantix -c "UPDATE users SET role='admin' WHERE username='username';"
```

Role changes take effect immediately — the `adminOnly` middleware queries the DB on each admin request rather than caching role in JWT.

## Security Notes

- The admin JWT itself contains no role claim — role is always queried from DB
- Promoting a user to admin does not require re-login
- Revoking admin access takes effect on the next admin API request
- `QUANTIX_ENCRYPTION_KEY` must be kept secret — it decrypts all user API keys
