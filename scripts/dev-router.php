<?php
// Router for PHP's built-in server in local development (scripts/dev.sh).
// Mirrors NGINX: existing static files are served directly, _lib/_view are
// never exposed, everything else goes to the front controller.
$root = dirname(__DIR__) . '/site';
$path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH) ?: '/';
if ($path !== '/' && !str_starts_with($path, '/_') && is_file($root . $path)) {
    return false;
}
require $root . '/index.php';
