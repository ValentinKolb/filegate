package domain

func (s *Service) CreateUploadSession(session UploadSession) error {
	return s.idx.Batch(func(b Batch) error {
		b.PutUploadSession(session)
		return nil
	})
}

func (s *Service) LookupUploadSession(sessionID string) (*UploadSession, error) {
	return s.idx.LookupUploadSession(sessionID)
}

func (s *Service) ListUploadSessions(phase UploadSessionPhase) ([]UploadSession, error) {
	return s.idx.ListUploadSessions(phase)
}

func (s *Service) UpdateUploadSession(session UploadSession) error {
	return s.idx.Batch(func(b Batch) error {
		b.PutUploadSession(session)
		return nil
	})
}

func (s *Service) DeleteUploadSession(sessionID string) error {
	return s.idx.Batch(func(b Batch) error {
		b.DelUploadSegments(sessionID)
		b.DelUploadSession(sessionID)
		return nil
	})
}

func (s *Service) PutUploadSegment(segment UploadSegment) error {
	return s.idx.Batch(func(b Batch) error {
		b.PutUploadSegment(segment)
		return nil
	})
}

func (s *Service) ListUploadSegments(sessionID string) ([]UploadSegment, error) {
	return s.idx.ListUploadSegments(sessionID)
}

func (s *Service) LookupUploadCommitRecord(sessionID string) (*UploadCommitRecord, error) {
	return s.idx.LookupUploadCommitRecord(sessionID)
}

func (s *Service) CommitUploadSessionState(session UploadSession, record UploadCommitRecord) error {
	return s.idx.Batch(func(b Batch) error {
		b.PutUploadSession(session)
		b.PutUploadCommitRecord(record)
		return nil
	})
}

func (s *Service) DeleteUploadCommitRecord(sessionID string) error {
	return s.idx.Batch(func(b Batch) error {
		b.DelUploadCommitRecord(sessionID)
		return nil
	})
}
