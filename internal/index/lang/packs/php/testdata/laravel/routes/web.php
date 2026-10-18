<?php

use App\Http\Controllers\UserController;
use App\Http\Controllers\{HomeController, PostController};
use Illuminate\Support\Facades\Route;

Route::get('/', [HomeController::class, 'index']);

Route::middleware('auth')->prefix('admin')->group(function () {
    Route::get('/users', [UserController::class, 'index']);
    Route::prefix('posts')->group(function () {
        Route::post('/{post}/publish', [PostController::class, 'publish']);
    });
});

Route::resource('photos', PhotoController::class);
Route::match(['get', 'post'], '/contact', [HomeController::class, 'contact']);

// Route::get('/commented-out', fn () => 1);
$doc = "Route::get('/in-a-string', fn () => 1)";
