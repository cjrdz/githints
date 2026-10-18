<?php

namespace App\Controllers;

use App\Models\UserModel;

class Home extends BaseController
{
    public function index(): string
    {
        return view('welcome', ['users' => (new UserModel())->findAll()]);
    }
}
