<?php
declare(strict_types=1);

// Front controller for listen.vrontier.org.
// NGINX sends every request that is not an existing static file here.
// Only paths listed in ROUTES render a page; everything else gets a real 404
// (never a soft-404 — see the nexdig / quest.de lessons).

require __DIR__ . '/_lib/bootstrap.php';

$path  = request_path();
$route = ROUTES[$path] ?? null;

if ($route === null) {
    http_response_code(404);
    render('404', ['title' => 'Not found']);
    exit;
}

render($route['view'], $route);
