import { BrowserRouter, Route, Routes } from "react-router";

export function Spa() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<p />} />
        <Route index element={<p />} />
        <Route path="/invoices/:invoiceId" element={<p />} />
      </Routes>
    </BrowserRouter>
  );
}
