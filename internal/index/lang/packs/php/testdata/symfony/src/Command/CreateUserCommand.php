<?php

namespace App\Command;

use Symfony\Component\Console\Attribute\AsCommand;
use Symfony\Component\Console\Command\Command;

#[AsCommand(name: 'app:create-user', description: 'Creates a user')]
final class CreateUserCommand extends Command
{
}
