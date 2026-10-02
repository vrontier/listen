<?php
declare(strict_types=1);

// Front controller for listen.vrontier.org.
// NGINX sends every request that is not an existing static file here.
// Only known pages and configured sources render; everything else gets a
// real 404 (never a soft-404 — see the nexdig / quest.de lessons).

require __DIR__ . '/_lib/bootstrap.php';

$path = request_path();

// For search engines: the pages and the listed sources (unlisted ones are
// noindex and stay out). robots.txt points here.
if ($path === '/sitemap.xml') {
    header('Content-Type: application/xml; charset=UTF-8');
    echo sitemap_xml();
    exit;
}

$route = route($path);

if ($route === null) {
    http_response_code(404);
    render('404', ['title' => 'Not found']);
    exit;
}

if (isset($route['redirect'])) {
    header('Location: ' . $route['redirect'], true, 302);
    exit;
}

if (($route['view'] ?? '') === 'contact') {
    require __DIR__ . '/_lib/contact.php';
    $route['form'] = contact_handle();
}

render($route['view'], $route);
