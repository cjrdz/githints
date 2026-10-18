import 'package:go_router/go_router.dart';
import 'screens/product_screen.dart';

final router = GoRouter(
  routes: [
    GoRoute(
      path: '/',
      builder: (context, state) => const ProductScreen(),
      routes: [
        GoRoute(
          name: 'detail',
          path: 'products/:id',
          builder: (context, state) => const ProductScreen(),
        ),
      ],
    ),
  ],
);
