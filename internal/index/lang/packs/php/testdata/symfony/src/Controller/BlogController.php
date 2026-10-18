<?php

namespace App\Controller;

use App\Entity\Post;
use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\HttpFoundation\Response;
use Symfony\Component\Routing\Attribute\Route;

#[Route('/blog', name: 'blog_')]
final class BlogController extends AbstractController
{
    #[Route('', name: 'index', methods: ['GET'])]
    public function index(): Response
    {
        return new Response();
    }

    #[Route(
        '/{slug}',
        name: 'show',
        methods: ['GET', 'HEAD'],
    )]
    #[Cache(maxage: 60)]
    public function show(Post $post): Response
    {
        return new Response();
    }

    # #[Route('/commented-out')]
    public function helper(): void
    {
    }
}
