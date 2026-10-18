import styled, { createGlobalStyle } from "styled-components";
import { Link } from "react-router-dom";

export const Button = styled.button`
  padding: 0.5rem 1rem;
`;

export const NavLink = styled(Link)`
  color: inherit;
`;

const Title: React.FC = styled.h1`
  font-size: 2rem;
`;

export const GlobalStyle = createGlobalStyle`
  body { margin: 0; }
`;

// A string mentioning styled.div is not a component.
const doc = "const Fake = styled.div`x`";
