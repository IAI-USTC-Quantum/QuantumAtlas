# 反向代理

`qatlasd` 默认监听 `127.0.0.1:4200`，**不直接暴露在公网**。前面应该挂 Caddy / nginx / Traefik 之类做：

- TLS 终结
- HTTP/2 / HTTP/3
- LE 自动证书
- gzip / brotli 压缩
- 静态资源缓存
- （可选）Host 路由 / 多 site

下面给 Caddy 和 nginx 两套模板，覆盖最常见的两种场景。

## 关键不变量

无论用什么反代，下面三条必须满足，否则会出微妙故障：

!!! danger "三条铁律"

    1. **`Host` header 必须 preserve**——RustFS 用 SigV4 验签把 Host 算进 canonical request，反代改写 Host 会让 presigned URL 返回 `SignatureDoesNotMatch`。
    2. **`/_/`、`/api/`、`/install-qatlasd.sh` 全部转发**——SPA catch-all 在 server 这边处理，不要在反代层面截留。
    3. **WebSocket 透传**——PocketBase realtime 用 SSE/WS；Caddy v2 默认透传，nginx 需要显式 `proxy_http_version 1.1` + `Upgrade` 头。

## Caddy（推荐）

最简：

```caddyfile title="/etc/caddy/Caddyfile"
quantum-atlas.ai {
    encode gzip zstd
    reverse_proxy 127.0.0.1:4200 {
        # 关键：preserve Host
        header_up Host {host}
    }
}
```

Caddy 自动从 Let's Encrypt 拿证书，自动 HTTP→HTTPS 重定向，自动 HTTP/2 + HTTP/3。

### 多 site（同台机部署 + RustFS 公网入口）

如果用 RustFS dual endpoint 模式，公网 RustFS 子域要单独反代到 RustFS：

```caddyfile
quantum-atlas.ai {
    encode gzip zstd
    reverse_proxy 127.0.0.1:4200 {
        header_up Host {host}
    }
}

# RustFS 公网入口（给 presigned URL 用）
<rustfs-public-host> {
    encode gzip zstd
    reverse_proxy <rustfs-internal-host>:9000 {
        # 关键：preserve Host，SigV4 才能验签通过
        header_up Host {host}
    }
}
```

详见 [RustFS 部署](rustfs.md#dual-endpoint)。

### 自签证书（开发 / 未备案 IP）

```caddyfile
# 直 IP 场景（未备案 / 内网）
https://<your-ip-or-host> {
    tls internal
    encode gzip zstd
    reverse_proxy 127.0.0.1:4200 {
        header_up Host {host}
    }
}

https://<your-ip-or-host>:9000 {
    tls internal
    reverse_proxy <rustfs-internal-host>:9000 {
        header_up Host {host}
    }
}
```

`tls internal` 用 Caddy 自带 CA 签 IP SAN 证书。client 必须接受自签——在 `~/.config/qatlas/config.yaml` 设 `insecure: true`（`qatlas config set insecure true`）。

### 加 GitHub OAuth 反代头审计（可选）

```caddyfile
atlas.example.com {
    @api path /api/*
    reverse_proxy @api 127.0.0.1:4200 {
        header_up Host {host}
        # 假设你前面有 caddy-security 之类做 SSO，这里把 GitHub username 注入
        header_up X-Token-Subject {http.auth.user.email}
    }
}
```

然后在 `.env` 配 `QATLAS_USER_HEADER=X-Token-Subject`——server 会把这个值
存进相关审计字段。

## nginx

```nginx title="/etc/nginx/sites-available/quantum-atlas.conf"
upstream qatlas {
    server 127.0.0.1:4200 fail_timeout=5s max_fails=3;
    keepalive 16;
}

server {
    listen 443 ssl http2;
    server_name quantum-atlas.ai;

    ssl_certificate     /etc/letsencrypt/live/quantum-atlas.ai/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/quantum-atlas.ai/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;

    gzip on;
    gzip_types text/plain text/css application/json application/javascript;

    client_max_body_size 200M;  # 上传 PDF 用

    location / {
        proxy_pass http://qatlas;

        # 关键：preserve Host
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # WebSocket / SSE 透传（PocketBase realtime）
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;

        # 大请求 buffer 调一下
        proxy_buffering off;
    }
}

# HTTP -> HTTPS
server {
    listen 80;
    server_name quantum-atlas.ai;
    return 301 https://$host$request_uri;
}
```

nginx 需要自己跑 certbot / acme.sh 拿 LE 证书，比 Caddy 多一步。

### RustFS public endpoint（nginx 版）

```nginx
server {
    listen 443 ssl http2;
    server_name <rustfs-public-host>;

    ssl_certificate     /etc/letsencrypt/live/<rustfs-public-host>/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/<rustfs-public-host>/privkey.pem;

    client_max_body_size 0;  # 不限大小（presigned URL 用）

    location / {
        proxy_pass http://<rustfs-internal-host>:9000;

        # ★★★ 必须 preserve Host
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;

        # 大文件 streaming
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_http_version 1.1;
    }
}
```

## Traefik / Apache / Cloudflare

类似的，关键还是那三条铁律。Cloudflare 默认会重写 Host header（在 "Network" → "Traffic" 设置 "Preserve original Host header" 或加 Page Rule）。

## 验证反代是否正确

```bash
# 1. 健康检查（应该返回 200 + JSON）
curl https://atlas.example.com/api/health | jq

# 2. 检查 Host header 是否 preserve
curl -sI https://atlas.example.com/api/health | grep -i 'access-control\|content-type'
```

如果 RustFS 公网入口 presign 出现 `SignatureDoesNotMatch`，**几乎一定是 Host
header 没 preserve**——回去检查反代配置。

## 多边缘 active-active

每条线路一台反代，对应一台 qatlasd。共享后端 (RustFS / Neo4j) 通过 EasyTier mesh 互通。各边缘 PocketBase 独立（用户/PAT 不跨节点）。
