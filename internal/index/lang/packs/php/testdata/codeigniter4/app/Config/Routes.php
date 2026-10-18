<?php

use CodeIgniter\Router\RouteCollection;

/**
 * @var RouteCollection $routes
 */
$routes->get('/', 'Home::index');
$routes->group('admin', static function ($routes) {
    $routes->get('users', 'Admin\Users::index');
    $routes->post('users/(:num)', 'Admin\Users::update/$1');
});
$routes->resource('photos');
