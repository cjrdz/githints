<?php

namespace App\Http\Controllers;

use App\Http\Requests\StorePostRequest;
use App\Jobs\PublishPost;
use App\Models\Post;

class PostController extends Controller
{
    public function publish(Post $post)
    {
        PublishPost::dispatch($post);
    }
}
