<?php

namespace App\Console\Commands;

use Illuminate\Console\Command;

class SendDigest extends Command
{
    protected $signature = 'digest:send {user?} {--queue}';
}
